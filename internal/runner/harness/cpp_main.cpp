#include <bits/stdc++.h>
#include <nlohmann/json.hpp>
#include "solution.cpp"

namespace codefight_judge {
using json = nlohmann::json;

template<class T> struct vector_type : std::false_type {};
template<class T, class A> struct vector_type<std::vector<T, A>> : std::true_type {
    using element = T;
};

template<class T> T decode(const json& value) {
    if constexpr (std::is_same_v<T, bool>) {
        if (!value.is_boolean()) throw std::runtime_error("expected boolean argument");
    } else if constexpr (std::is_integral_v<T>) {
        if (!value.is_number_integer()) throw std::runtime_error("expected integer argument");
        if (value.is_number_unsigned()) {
            auto number = value.get<std::uint64_t>();
            if (number > static_cast<std::uint64_t>(std::numeric_limits<T>::max()))
                throw std::runtime_error("integer argument is out of range");
        } else {
            auto number = value.get<std::int64_t>();
            if constexpr (std::is_unsigned_v<T>) {
                if (number < 0 || static_cast<std::uint64_t>(number) > std::numeric_limits<T>::max())
                    throw std::runtime_error("integer argument is out of range");
            } else if (number < std::numeric_limits<T>::lowest() || number > std::numeric_limits<T>::max()) {
                throw std::runtime_error("integer argument is out of range");
            }
        }
    } else if constexpr (std::is_floating_point_v<T>) {
        if (!value.is_number()) throw std::runtime_error("expected numeric argument");
        auto number = value.get<T>();
        if (!std::isfinite(number)) throw std::runtime_error("numeric argument is out of range");
    } else if constexpr (vector_type<T>::value) {
        if (!value.is_array()) throw std::runtime_error("expected array argument");
        T result;
        result.reserve(value.size());
        for (const auto& item : value) result.push_back(decode<typename vector_type<T>::element>(item));
        return result;
    }
    return value.get<T>();
}

template<class T> struct signature;
template<class R, class... A> struct signature<R(*)(A...)> {
    using arguments = std::tuple<std::decay_t<A>...>;
    static constexpr std::size_t count = sizeof...(A);
    static_assert(!std::is_void_v<R>, "Solve must return one value");
};
template<class R, class... A> struct signature<R(*)(A...) noexcept> : signature<R(*)(A...)> {};

std::string single_string(const std::string& input) {
    auto value = json::parse(input, nullptr, false);
    if (value.is_string()) return value.get<std::string>();
    auto result = input;
    while (!result.empty() && (result.back() == '\n' || result.back() == '\r')) result.pop_back();
    return result;
}

template<class Tuple, std::size_t... I>
void read_arguments(std::istream& input, Tuple& arguments, std::index_sequence<I...>) {
    auto read = [&]<std::size_t Index>() {
        json value;
        input >> value;
        std::get<Index>(arguments) = decode<std::tuple_element_t<Index, Tuple>>(value);
    };
    (read.template operator()<I>(), ...);
    input >> std::ws;
    if (input.peek() != std::char_traits<char>::eof()) throw std::runtime_error("unexpected input after last argument");
}

void finite_result(const json& result) {
    if (result.is_number_float() && !std::isfinite(result.get<double>()))
        throw std::runtime_error("Solve returned a non-finite number");
    if (result.is_structured()) for (const auto& item : result) finite_result(item);
}

template<class F> void run(F function) {
    using traits = signature<F>;
    using tuple = typename traits::arguments;
    std::string input((std::istreambuf_iterator<char>(std::cin)), {});
    tuple arguments;
    if constexpr (traits::count == 1) {
        if constexpr (std::is_same_v<std::tuple_element_t<0, tuple>, std::string>) {
            std::get<0>(arguments) = single_string(input);
        } else {
            std::istringstream stream(input);
            read_arguments(stream, arguments, std::make_index_sequence<traits::count>{});
        }
    } else {
        std::istringstream stream(input);
        read_arguments(stream, arguments, std::make_index_sequence<traits::count>{});
    }
    auto result = std::apply(function, arguments);
    if constexpr (std::is_same_v<std::decay_t<decltype(result)>, std::string>) {
        std::cout << result;
    } else {
        json output = result;
        finite_result(output);
        std::cout << output.dump();
    }
}
}

int main() {
    try { codefight_judge::run(&__ENTRY__); }
    catch (const std::exception& error) {
        std::cerr << "invalid input or solution result: " << error.what() << '\n';
        return 1;
    }
}
